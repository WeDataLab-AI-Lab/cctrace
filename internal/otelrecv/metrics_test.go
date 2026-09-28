package otelrecv

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func f64(v float64) *float64 { return &v }

// resetMetricDropCounts clears the process-wide drop counters so that
// log-assertion tests do not depend on run order or on -count=1.
func resetMetricDropCounts(t *testing.T) {
	t.Helper()
	reset := func() {
		metricDropCounts = &sync.Map{}
		// The key counter and the rate-limit clock are separate globals; leaving
		// either set makes later tests take a different code path than they do
		// in isolation.
		metricDropKeys.Store(0)
		lastUntrackedDropAt.Store(0)
	}
	reset()
	t.Cleanup(reset)
}

func histogramMetric(name string, dps ...*metricspb.HistogramDataPoint) *metricspb.Metric {
	return &metricspb.Metric{
		Name: name,
		Data: &metricspb.Metric_Histogram{
			Histogram: &metricspb.Histogram{DataPoints: dps},
		},
	}
}

func claudeRes() resourceInfo {
	return resourceInfo{serviceName: "claude-code", sessionID: "res-session"}
}

// 1. Histogram datapoint attributes must survive into Dimensions.
func TestParseMetric_Histogram_PreservesAttributes(t *testing.T) {
	m := histogramMetric("codex.turn.token_usage", &metricspb.HistogramDataPoint{
		TimeUnixNano: 1700000000000000000,
		Count:        3,
		Sum:          f64(120),
		Attributes: []*commonpb.KeyValue{
			mkKV("token.type", strAV("input")),
			mkKV("cached", boolAV(true)),
			mkKV("slot", intAV(7)),
		},
	})

	got, _ := parseMetric(m, resourceInfo{serviceName: "codex"})
	if len(got) != 1 {
		t.Fatalf("want 1 metric, got %d", len(got))
	}
	om := got[0]
	if om.Dimensions["token.type"] != "input" {
		t.Errorf("token.type = %#v, want \"input\"", om.Dimensions["token.type"])
	}
	if om.Dimensions["cached"] != true {
		t.Errorf("cached = %#v, want true", om.Dimensions["cached"])
	}
	if om.Dimensions["slot"] != int64(7) {
		t.Errorf("slot = %#v, want int64(7)", om.Dimensions["slot"])
	}
	if om.ValueInt == nil || *om.ValueInt != 3 {
		t.Errorf("ValueInt = %v, want 3", om.ValueInt)
	}
	if om.ValueDouble == nil || *om.ValueDouble != 120 {
		t.Errorf("ValueDouble = %v, want 120", om.ValueDouble)
	}
	if om.Agent != "codex" || om.BillingProvider != "openai" {
		t.Errorf("agent/provider = %q/%q, want codex/openai", om.Agent, om.BillingProvider)
	}
}

// 2. model / session_id are promoted to dedicated columns and removed from Dimensions.
func TestParseMetric_Histogram_PromotesModelAndSessionID(t *testing.T) {
	m := histogramMetric("codex.turn.token_usage", &metricspb.HistogramDataPoint{
		Count: 1,
		Sum:   f64(10),
		Attributes: []*commonpb.KeyValue{
			mkKV("model", strAV("gpt-5-codex")),
			mkKV("session_id", strAV("dp-session")),
			mkKV("token.type", strAV("output")),
		},
	})

	got, _ := parseMetric(m, resourceInfo{serviceName: "codex", sessionID: "res-session"})
	if len(got) != 1 {
		t.Fatalf("want 1 metric, got %d", len(got))
	}
	om := got[0]
	if om.Model != "gpt-5-codex" {
		t.Errorf("Model = %q, want gpt-5-codex", om.Model)
	}
	if om.SessionID != "dp-session" {
		t.Errorf("SessionID = %q, want dp-session", om.SessionID)
	}
	if _, ok := om.Dimensions["model"]; ok {
		t.Error("model must be removed from Dimensions")
	}
	if _, ok := om.Dimensions["session_id"]; ok {
		t.Error("session_id must be removed from Dimensions")
	}
	if om.Dimensions["token.type"] != "output" {
		t.Errorf("token.type = %#v, want \"output\"", om.Dimensions["token.type"])
	}
}

// 3. Regression: attribute-less histogram keeps ValueInt/ValueDouble and has empty Dimensions.
func TestParseMetric_Histogram_NoAttributes(t *testing.T) {
	m := histogramMetric("claude_code.token.usage", &metricspb.HistogramDataPoint{
		Count: 42,
		Sum:   f64(3.5),
	})

	got, _ := parseMetric(m, claudeRes())
	if len(got) != 1 {
		t.Fatalf("want 1 metric, got %d", len(got))
	}
	om := got[0]
	if len(om.Dimensions) != 0 {
		t.Errorf("Dimensions = %#v, want empty", om.Dimensions)
	}
	if om.ValueInt == nil || *om.ValueInt != 42 {
		t.Errorf("ValueInt = %v, want 42", om.ValueInt)
	}
	if om.ValueDouble == nil || *om.ValueDouble != 3.5 {
		t.Errorf("ValueDouble = %v, want 3.5", om.ValueDouble)
	}
	if om.SessionID != "res-session" {
		t.Errorf("SessionID = %q, want res-session", om.SessionID)
	}
}

// 3b. Histogram without Sum leaves ValueDouble nil.
func TestParseMetric_Histogram_NoSum(t *testing.T) {
	m := histogramMetric("claude_code.token.usage", &metricspb.HistogramDataPoint{Count: 9})

	got, _ := parseMetric(m, claudeRes())
	if len(got) != 1 {
		t.Fatalf("want 1 metric, got %d", len(got))
	}
	if got[0].ValueDouble != nil {
		t.Errorf("ValueDouble = %v, want nil", *got[0].ValueDouble)
	}
	if got[0].ValueInt == nil || *got[0].ValueInt != 9 {
		t.Errorf("ValueInt = %v, want 9", got[0].ValueInt)
	}
}

// 4. Attribute keys literally named "sum"/"count" must not be clobbered.
func TestParseMetric_Histogram_SumCountAttributeKeys(t *testing.T) {
	m := histogramMetric("codex.turn.token_usage", &metricspb.HistogramDataPoint{
		Count: 1,
		Sum:   f64(999),
		Attributes: []*commonpb.KeyValue{
			mkKV("sum", strAV("attr-sum")),
			mkKV("count", strAV("attr-count")),
		},
	})

	got, _ := parseMetric(m, resourceInfo{serviceName: "codex"})
	if len(got) != 1 {
		t.Fatalf("want 1 metric, got %d", len(got))
	}
	om := got[0]
	if om.Dimensions["sum"] != "attr-sum" {
		t.Errorf("dimensions[sum] = %#v, want \"attr-sum\"", om.Dimensions["sum"])
	}
	if om.Dimensions["count"] != "attr-count" {
		t.Errorf("dimensions[count] = %#v, want \"attr-count\"", om.Dimensions["count"])
	}
}

// 5. Lock in: claude resource + non-claude model attribute => (claude, other).
func TestParseMetric_Histogram_ClaudeResourceNonClaudeModel(t *testing.T) {
	m := histogramMetric("claude_code.token.usage", &metricspb.HistogramDataPoint{
		Count:      1,
		Sum:        f64(1),
		Attributes: []*commonpb.KeyValue{mkKV("model", strAV("kimi-k2"))},
	})

	got, _ := parseMetric(m, claudeRes())
	if len(got) != 1 {
		t.Fatalf("want 1 metric, got %d", len(got))
	}
	if got[0].Agent != "claude" {
		t.Errorf("Agent = %q, want claude", got[0].Agent)
	}
	if got[0].BillingProvider != "other" {
		t.Errorf("BillingProvider = %q, want other", got[0].BillingProvider)
	}
}

// 5b. Lock in: claude resource + claude model attribute stays anthropic.
func TestParseMetric_Histogram_ClaudeResourceClaudeModel(t *testing.T) {
	m := histogramMetric("claude_code.token.usage", &metricspb.HistogramDataPoint{
		Count:      1,
		Sum:        f64(1),
		Attributes: []*commonpb.KeyValue{mkKV("model", strAV("claude-opus-4-8"))},
	})

	got, _ := parseMetric(m, claudeRes())
	if len(got) != 1 {
		t.Fatalf("want 1 metric, got %d", len(got))
	}
	if got[0].Agent != "claude" || got[0].BillingProvider != "anthropic" {
		t.Errorf("agent/provider = %q/%q, want claude/anthropic", got[0].Agent, got[0].BillingProvider)
	}
}

// 6. Unsupported metric types are dropped without panicking, and are logged.
func TestParseMetric_UnsupportedType(t *testing.T) {
	cases := []struct {
		name   string
		metric *metricspb.Metric
	}{
		{
			name: "unsupported.exponential.histogram",
			metric: &metricspb.Metric{
				Name: "unsupported.exponential.histogram",
				Data: &metricspb.Metric_ExponentialHistogram{
					ExponentialHistogram: &metricspb.ExponentialHistogram{
						DataPoints: []*metricspb.ExponentialHistogramDataPoint{
							{Count: 5, Sum: f64(100)},
						},
					},
				},
			},
		},
		{
			name: "unsupported.summary",
			metric: &metricspb.Metric{
				Name: "unsupported.summary",
				Data: &metricspb.Metric_Summary{
					Summary: &metricspb.Summary{
						DataPoints: []*metricspb.SummaryDataPoint{{Count: 5, Sum: 100}},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMetricDropCounts(t)
			var buf bytes.Buffer
			prev := log.Writer()
			log.SetOutput(&buf)
			defer log.SetOutput(prev)

			got, _ := parseMetric(tc.metric, resourceInfo{serviceName: "codex"})
			if len(got) != 0 {
				t.Fatalf("want 0 metrics, got %d", len(got))
			}
			out := buf.String()
			if !strings.Contains(out, "unsupported metric type") || !strings.Contains(out, tc.name) {
				t.Errorf("log output missing unsupported-type notice: %q", out)
			}
		})
	}
}

// 6b. Repeated drops of the same metric are not re-logged on every export: the
// backoff logs the 1st, 10th, 100th, so two exports produce exactly one line.
func TestParseMetric_DropLogBacksOff(t *testing.T) {
	m := &metricspb.Metric{
		Name: "unsupported.once.only",
		Data: &metricspb.Metric_Summary{
			Summary: &metricspb.Summary{DataPoints: []*metricspb.SummaryDataPoint{{Count: 1}}},
		},
	}

	resetMetricDropCounts(t)
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	_, _ = parseMetric(m, resourceInfo{})
	_, _ = parseMetric(m, resourceInfo{})

	// Count the log header, not the metric name: the drop line also embeds a
	// JSON sample of the payload, which repeats the name inside the same line.
	if n := strings.Count(buf.String(), `dropped metric "unsupported.once.only"`); n != 1 {
		t.Errorf("logged %d times, want 1: %q", n, buf.String())
	}
}

// #752: identical codex.tool.call delta rows cannot be told apart — an exporter
// retry and two datapoints stamped the same instant look the same when only
// TimeUnixNano is kept. The datapoint's start time is the evidence that splits
// them, so it is kept in dimensions (as a string: nanoseconds overflow a JSON
// float). An unset start time (0, usual for gauges) adds nothing.
func TestParseMetric_KeepsDatapointStartTime(t *testing.T) {
	const start = uint64(1_758_500_000_123_456_789)
	sum := &metricspb.Metric{
		Name: "codex.tool.call",
		Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{DataPoints: []*metricspb.NumberDataPoint{{
			StartTimeUnixNano: start,
			TimeUnixNano:      start + 60_000_000_000,
			Value:             &metricspb.NumberDataPoint_AsInt{AsInt: 1},
			Attributes:        []*commonpb.KeyValue{mkKV("tool", strAV("exec"))},
		}}}},
	}
	got, _ := parseMetric(sum, resourceInfo{serviceName: "codex_exec"})
	if len(got) != 1 {
		t.Fatalf("want 1 metric, got %d", len(got))
	}
	if v := got[0].Dimensions[metricStartTimeKey]; v != "1758500000123456789" {
		t.Errorf("dimensions[%s] = %#v, want the exact start time", metricStartTimeKey, v)
	}
	if got[0].Dimensions["tool"] != "exec" || got[0].Agent != "codex" {
		t.Errorf("start time disturbed the row: dims=%#v agent=%q", got[0].Dimensions, got[0].Agent)
	}

	hist := histogramMetric("codex.turn.duration", &metricspb.HistogramDataPoint{StartTimeUnixNano: start, Count: 1})
	if got, _ := parseMetric(hist, resourceInfo{serviceName: "codex"}); got[0].Dimensions[metricStartTimeKey] != "1758500000123456789" {
		t.Errorf("histogram dimensions = %#v, want start time", got[0].Dimensions)
	}

	gauge := &metricspb.Metric{
		Name: "claude_code.active",
		Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
			Value: &metricspb.NumberDataPoint_AsInt{AsInt: 1},
		}}}},
	}
	if got, _ := parseMetric(gauge, claudeRes()); len(got[0].Dimensions) != 0 {
		t.Errorf("unset start time added dimensions %#v", got[0].Dimensions)
	}
}

// Regression: Sum / Gauge branches keep behaving as before.
func TestParseMetric_SumAndGauge(t *testing.T) {
	sum := &metricspb.Metric{
		Name: "claude_code.cost.usage",
		Data: &metricspb.Metric_Sum{
			Sum: &metricspb.Sum{DataPoints: []*metricspb.NumberDataPoint{{
				Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 0.25},
				Attributes: []*commonpb.KeyValue{
					mkKV("model", strAV("claude-sonnet-4")),
					mkKV("session.id", strAV("sum-session")),
					mkKV("type", strAV("output")),
				},
			}}},
		},
	}

	got, _ := parseMetric(sum, claudeRes())
	if len(got) != 1 {
		t.Fatalf("want 1 metric, got %d", len(got))
	}
	om := got[0]
	if om.Model != "claude-sonnet-4" || om.SessionID != "sum-session" {
		t.Errorf("model/session = %q/%q", om.Model, om.SessionID)
	}
	if om.ValueDouble == nil || *om.ValueDouble != 0.25 {
		t.Errorf("ValueDouble = %v, want 0.25", om.ValueDouble)
	}
	if om.Dimensions["type"] != "output" {
		t.Errorf("dimensions[type] = %#v, want \"output\"", om.Dimensions["type"])
	}
	if om.Agent != "claude" || om.BillingProvider != "anthropic" {
		t.Errorf("agent/provider = %q/%q, want claude/anthropic", om.Agent, om.BillingProvider)
	}

	gauge := &metricspb.Metric{
		Name: "claude_code.active",
		Data: &metricspb.Metric_Gauge{
			Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{{
				Value:      &metricspb.NumberDataPoint_AsInt{AsInt: 11},
				Attributes: []*commonpb.KeyValue{mkKV("state", strAV("busy"))},
			}}},
		},
	}

	got, _ = parseMetric(gauge, claudeRes())
	if len(got) != 1 {
		t.Fatalf("want 1 metric, got %d", len(got))
	}
	if got[0].ValueInt == nil || *got[0].ValueInt != 11 {
		t.Errorf("ValueInt = %v, want 11", got[0].ValueInt)
	}
	if got[0].Dimensions["state"] != "busy" {
		t.Errorf("dimensions[state] = %#v, want \"busy\"", got[0].Dimensions["state"])
	}
}

// A datapoint that produces no row must be reported back to the sender.
// Answering a bare OK makes the loss invisible: the sender counts the export as
// delivered and nothing in the database shows the gap.
// A datapoint that produces no row must be reported back to the sender.
// Answering a bare OK makes the loss invisible: the sender counts the export as
// delivered and nothing in the database shows the gap.
//
// Only unsupported types are exercised here. The "empty Sum/Gauge/Histogram
// payload" states cannot occur over the wire -- protobuf always materializes a
// oneof that is set, verified by TestEmptyPayloadIsUnreachableOverTheWire --
// so asserting on them would prove nothing about real traffic.
func TestParseMetricReportsDroppedPayloads(t *testing.T) {
	resetMetricDropCounts(t)

	cases := []struct {
		name   string
		metric *metricspb.Metric
		points int64
	}{
		{
			name: "summary",
			metric: &metricspb.Metric{Name: "claude_code.summary", Data: &metricspb.Metric_Summary{
				Summary: &metricspb.Summary{DataPoints: []*metricspb.SummaryDataPoint{{Count: 1}, {Count: 2}}},
			}},
			points: 2,
		},
		{
			name: "exponential histogram",
			metric: &metricspb.Metric{Name: "claude_code.expo", Data: &metricspb.Metric_ExponentialHistogram{
				ExponentialHistogram: &metricspb.ExponentialHistogram{
					DataPoints: []*metricspb.ExponentialHistogramDataPoint{{Count: 3}},
				},
			}},
			points: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, drop := parseMetric(c.metric, resourceInfo{})
			if len(rows) != 0 {
				t.Fatalf("rows = %d, want 0", len(rows))
			}
			if drop == nil {
				t.Fatal("no drop reported; the loss would be invisible to the sender")
			}
			if drop.name != c.metric.Name {
				t.Errorf("drop.name = %q, want %q", drop.name, c.metric.Name)
			}
			if !strings.Contains(drop.reason, "unsupported metric type") {
				t.Errorf("drop.reason = %q, want it to mention the unsupported type", drop.reason)
			}
			// The count must reach rejected_data_points, otherwise the sender is
			// told "0 rejected" while losing real datapoints.
			if drop.dataPoints != c.points {
				t.Errorf("drop.dataPoints = %d, want %d", drop.dataPoints, c.points)
			}
		})
	}
}

// The nil-payload guards in parseMetric are unreachable from the network: a
// protobuf oneof that is set always decodes to a non-nil message. This is what
// justifies not reporting those branches as drops.
func TestEmptyPayloadIsUnreachableOverTheWire(t *testing.T) {
	for _, m := range []*metricspb.Metric{
		{Name: "s", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{}}},
		{Name: "s2", Data: &metricspb.Metric_Sum{}},
	} {
		wire, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var got metricspb.Metric
		if err := proto.Unmarshal(wire, &got); err != nil {
			t.Fatal(err)
		}
		if d, ok := got.Data.(*metricspb.Metric_Sum); ok && d.Sum == nil {
			t.Errorf("%s: decoded to a nil Sum, so the guard is reachable after all", m.Name)
		}
	}
}

// A metric that parses normally must not be reported as a drop.
func TestParseMetricReportsNoDropOnSuccess(t *testing.T) {
	resetMetricDropCounts(t)

	m := histogramMetric("claude_code.token.usage", &metricspb.HistogramDataPoint{
		Count: 2,
		Sum:   f64(30),
	})
	rows, drop := parseMetric(m, resourceInfo{})
	if drop != nil {
		t.Fatalf("unexpected drop: %+v", drop)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
}

// Repeated drops must keep leaving a trail rather than being suppressed after
// the first line — otherwise unbounded loss hides behind one log entry.
func TestDropLogMilestones(t *testing.T) {
	var logged []int64
	for n := int64(1); n <= 1000; n++ {
		if isDropLogMilestone(n) {
			logged = append(logged, n)
		}
	}
	want := []int64{1, 10, 100, 1000}
	if len(logged) != len(want) {
		t.Fatalf("milestones = %v, want %v", logged, want)
	}
	for i := range want {
		if logged[i] != want[i] {
			t.Fatalf("milestones = %v, want %v", logged, want)
		}
	}
}

// Two different metric names must be counted separately. Folding every name
// into one counter would still pass the backoff tests -- they reuse a single
// name -- while silently destroying the per-name diagnostics.
func TestDropCounterIsPerName(t *testing.T) {
	resetMetricDropCounts(t)

	a, b := dropCounter("metric.a"), dropCounter("metric.b")
	if a == nil || b == nil {
		t.Fatal("counters must be tracked while under the cap")
	}
	if a == b {
		t.Fatal("distinct names share a counter; per-name counting is gone")
	}
	a.Add(1)
	if b.Load() != 0 {
		t.Errorf("counting %q advanced the counter for %q", "metric.a", "metric.b")
	}
}

// Both names must log their own first drop. With a shared counter the second
// name is silently swallowed.
func TestDropLogsEachNewNameOnce(t *testing.T) {
	resetMetricDropCounts(t)
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	for _, name := range []string{"first.summary", "second.summary"} {
		_, _ = parseMetric(&metricspb.Metric{
			Name: name,
			Data: &metricspb.Metric_Summary{Summary: &metricspb.Summary{
				DataPoints: []*metricspb.SummaryDataPoint{{Count: 1}},
			}},
		}, resourceInfo{})
	}
	for _, name := range []string{"first.summary", "second.summary"} {
		if !strings.Contains(buf.String(), name) {
			t.Errorf("drop of %q was never logged: %q", name, buf.String())
		}
	}
}

// A flood of invented names bounds memory, but it must not silence the drops
// that matter. The HTTP OTLP port has no auth interceptor, so the name space is
// attacker controlled: if exceeding the cap muted logging for good, one request
// could permanently blind the only server-side view of data loss.
func TestGenuineDropsStillLogAfterNameFlood(t *testing.T) {
	resetMetricDropCounts(t)

	// Each invented name is a real drop, so a shared bucket would also be
	// counted up -- which is what pushes the next log milestone out of reach.
	for i := 0; i < metricDropKeyCap*2; i++ {
		if c := dropCounter(fmt.Sprintf("invented.%d", i)); c != nil {
			c.Add(1)
		}
	}
	tracked := 0
	metricDropCounts.Range(func(_, _ any) bool { tracked++; return true })
	if tracked > metricDropKeyCap {
		t.Errorf("tracked %d names, want at most %d", tracked, metricDropKeyCap)
	}

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	_, _ = parseMetric(&metricspb.Metric{
		Name: "claude_code.real.loss",
		Data: &metricspb.Metric_Summary{Summary: &metricspb.Summary{
			DataPoints: []*metricspb.SummaryDataPoint{{Count: 1}},
		}},
	}, resourceInfo{})

	if !strings.Contains(buf.String(), "claude_code.real.loss") {
		t.Errorf("a genuine drop was silenced by the name flood: %q", buf.String())
	}
}
