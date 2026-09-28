package otelrecv

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cctrace/internal/buffer"
	"cctrace/internal/emailalias"
	"cctrace/internal/queue"
	"cctrace/internal/store"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// MetricsReceiver implements the OTLP MetricsService and enqueues to PGMQ.
type MetricsReceiver struct {
	colmetricspb.UnimplementedMetricsServiceServer
	queue   *queue.Queue
	buffer  *buffer.Ring
	aliases emailalias.Resolver
	// identityResolver turns a verified ingest token into the account it belongs
	// to, for the gRPC path.
	identityResolver IdentityResolver

	// dropSession refuses metrics for sessions the dashboard deleted; see
	// LogsReceiver for why the drop happens at receive time.
	dropSession SessionFilter
	// dropAccount refuses metrics from excluded login addresses (#715).
	dropAccount AccountFilter
	// dropBilling refuses metrics of excluded billing accounts, as the sync
	// handler does for session records (#719).
	dropBilling BillingAccountFilter
}

// BillingAccountFilter reports whether telemetry of this billing account must be
// dropped. A plain function for the same reason SessionFilter is.
type BillingAccountFilter func(provider, accountID string) bool

// WithBillingAccountFilter installs the billing-account test. Without it every
// account is accepted.
func (r *MetricsReceiver) WithBillingAccountFilter(f BillingAccountFilter) *MetricsReceiver {
	r.dropBilling = f
	return r
}

// CodexAccountHeader is the header the Codex exporter names its billing account
// in; codexconfig writes it into ~/.codex/config.toml (#715). Codex metrics carry
// no session and no account of their own, so this is the only way to know which
// account a metric was billed to.
const CodexAccountHeader = "X-Cctrace-Codex-Account"

// maxAccountIDLen bounds the caller-supplied header before it is stored.
const maxAccountIDLen = 128

type codexAccountKey struct{}

// withCodexAccount carries the header value from the HTTP handler to the export.
func withCodexAccount(ctx context.Context, accountID string) context.Context {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" || len(accountID) > maxAccountIDLen {
		return ctx
	}
	return context.WithValue(ctx, codexAccountKey{}, accountID)
}

func codexAccountFromContext(ctx context.Context) string {
	accountID, _ := ctx.Value(codexAccountKey{}).(string)
	return accountID
}

// WithSessionFilter installs the drop test; see LogsReceiver.WithSessionFilter.
func (r *MetricsReceiver) WithSessionFilter(f SessionFilter) *MetricsReceiver {
	r.dropSession = f
	return r
}

// WithAccountFilter installs the account test; see LogsReceiver.WithAccountFilter.
func (r *MetricsReceiver) WithAccountFilter(f AccountFilter) *MetricsReceiver {
	r.dropAccount = f
	return r
}

func NewMetricsReceiver(q *queue.Queue, buf *buffer.Ring, aliases emailalias.Resolver) *MetricsReceiver {
	return &MetricsReceiver{queue: q, buffer: buf, aliases: aliases}
}

func (r *MetricsReceiver) Export(ctx context.Context, req *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	// The gRPC path arrives here; its interceptor leaves the verified token in
	// the context (#535).
	return r.ExportWithIdentity(ctx, req, r.identityFromContext(ctx))
}

func (r *MetricsReceiver) identityFromContext(ctx context.Context) *ClientIdentity {
	return identityFromContext(ctx, r.identityResolver)
}

// WithIdentityResolver installs the lookup used by the gRPC path.
func (r *MetricsReceiver) WithIdentityResolver(resolver IdentityResolver) *MetricsReceiver {
	r.identityResolver = resolver
	return r
}

func (r *MetricsReceiver) ExportWithIdentity(ctx context.Context, req *colmetricspb.ExportMetricsServiceRequest, identity *ClientIdentity) (*colmetricspb.ExportMetricsServiceResponse, error) {
	var metrics []*store.OtelMetric
	codexAccount := codexAccountFromContext(ctx)
	var rejected int64
	var dropNotes []string
	var droppedMetrics int

	for _, rm := range req.ResourceMetrics {
		res := extractResource(rm.Resource)
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				parsed, drop := parseMetric(m, res)
				if drop != nil {
					rejected += drop.dataPoints
					// Only the first few notes are kept. Both the number of
					// drops and the length of each metric name are caller
					// controlled, so joining every note and truncating
					// afterwards would let one request build a huge string to
					// throw away.
					if len(dropNotes) < dropNotesMax {
						dropNotes = append(dropNotes, fmt.Sprintf("%s: %s", drop.name, drop.reason))
					}
					droppedMetrics++
				}
				for _, om := range parsed {
					if r.dropSession != nil && r.dropSession(om.SessionID) {
						continue
					}
					// Only a Codex row is billed to the Codex account the header names.
					if om.Agent == "codex" {
						om.AccountID = codexAccount
					}
					if om.AccountID != "" && r.dropBilling != nil && r.dropBilling(om.BillingProvider, om.AccountID) {
						continue
					}
					// Judged before the identity fill below. A metric that named no
					// address gets the token owner's dashboard address, which is not
					// the Anthropic login that sent it; refusing on that would drop
					// whoever holds the token (#715).
					if r.dropAccount != nil && r.dropAccount(om.LoginEmail) {
						continue
					}
					metrics = append(metrics, om)
				}
			}
		}
	}

	// Overrides rather than fills a gap. Filling only what is empty leaves a
	// claimed value winning whenever there is one, which is the case that
	// matters: an empty field is a client that did not say, not a client that
	// said someone else (#535).
	//
	// login_email stays as the payload had it -- it names the Anthropic account,
	// and a cctrace token proves a cctrace dashboard account. user_team follows
	// the profile it is a property of.
	if identity != nil {
		for _, m := range metrics {
			if identity.ProfileEmail != "" {
				m.ProfileEmail = identity.ProfileEmail
			}
			if identity.UserID != "" {
				m.UserID = identity.UserID
			}
			if identity.ProfileEmail != "" || m.UserTeam == "" {
				m.UserTeam = identity.UserTeam
			}
			if m.LoginEmail == "" {
				m.LoginEmail = identity.LoginEmail
			}
		}
	}

	// Resolve email aliases
	for _, m := range metrics {
		m.ProfileEmail = r.aliases.Resolve(m.ProfileEmail)
	}

	if len(metrics) > 0 {
		for _, m := range metrics {
			if _, err := r.queue.Send(ctx, queue.QueueOtelMetrics, m); err != nil {
				log.Printf("[otelrecv] failed to enqueue metric, buffering: %v", err)
				data, merr := json.Marshal(m)
				if merr != nil {
					log.Printf("[otelrecv] failed to marshal metric for buffer: %v", merr)
					continue
				}
				r.buffer.Push(data)
				continue
			}
		}
		log.Printf("[otelrecv] enqueued %d metrics", len(metrics))
	}

	resp := &colmetricspb.ExportMetricsServiceResponse{}
	if len(dropNotes) > 0 {
		// Tell the sender about the loss instead of answering a bare OK.
		// rejected_data_points may legitimately be 0 (an unknown type carries no
		// readable payload), in which case error_message acts as the warning.
		// The "+N more" count is appended after truncating, so it survives:
		// it is the part that tells the sender the list is incomplete.
		var tail string
		if droppedMetrics > len(dropNotes) {
			tail = fmt.Sprintf("; (+%d more)", droppedMetrics-len(dropNotes))
		}
		msg := limit(strings.Join(dropNotes, "; "), dropMessageMaxBytes-len(tail)) + tail
		resp.PartialSuccess = &colmetricspb.ExportMetricsPartialSuccess{
			RejectedDataPoints: rejected,
			ErrorMessage:       msg,
		}
	}
	return resp, nil
}

func parseMetric(m *metricspb.Metric, res resourceInfo) ([]*store.OtelMetric, *metricDrop) {
	var result []*store.OtelMetric

	switch d := m.Data.(type) {
	case *metricspb.Metric_Sum:
		if d.Sum == nil {
			break
		}
		for _, dp := range d.Sum.DataPoints {
			om := newOtelMetric(m.Name, dp.TimeUnixNano, res)
			om.Dimensions = withStartTime(flattenNumberDPAttrs(dp), dp.StartTimeUnixNano)
			om.Agent, om.BillingProvider = classifyAgent(om.Dimensions, res)
			setNumberValue(om, dp)
			extractMetricFields(om)
			result = append(result, om)
		}

	case *metricspb.Metric_Gauge:
		if d.Gauge == nil {
			break
		}
		for _, dp := range d.Gauge.DataPoints {
			om := newOtelMetric(m.Name, dp.TimeUnixNano, res)
			om.Dimensions = withStartTime(flattenNumberDPAttrs(dp), dp.StartTimeUnixNano)
			om.Agent, om.BillingProvider = classifyAgent(om.Dimensions, res)
			setNumberValue(om, dp)
			extractMetricFields(om)
			result = append(result, om)
		}

	case *metricspb.Metric_Histogram:
		if d.Histogram == nil {
			break
		}
		for _, dp := range d.Histogram.DataPoints {
			om := newOtelMetric(m.Name, dp.TimeUnixNano, res)
			om.Dimensions = withStartTime(flattenHistogramDPAttrs(dp), dp.StartTimeUnixNano)
			om.Agent, om.BillingProvider = classifyAgent(om.Dimensions, res)
			count := int64(dp.Count)
			om.ValueInt = &count
			if dp.Sum != nil {
				sum := dp.GetSum()
				om.ValueDouble = &sum
			}
			extractMetricFields(om)
			result = append(result, om)
		}

	default:
		return nil, dropMetric(m, res, fmt.Sprintf("unsupported metric type %T", m.Data))
	}

	// Every supported branch appends one row per datapoint, so a supported type
	// cannot reach here having lost anything. A "produced no rows" guard was
	// tried and removed: no input could reach it, which is the same untestable
	// shape as the nil-payload branches above.
	return result, nil
}

func newOtelMetric(name string, timeNano uint64, res resourceInfo) *store.OtelMetric {
	ts := time.Now()
	if timeNano > 0 {
		ts = time.Unix(0, int64(timeNano))
	}
	return &store.OtelMetric{
		Ts:              ts,
		MetricName:      name,
		SessionID:       res.sessionID,
		UserID:          res.userID,
		ProfileEmail:    res.userProfileEmail,
		LoginEmail:      res.userEmail,
		UserTeam:        res.userTeam,
		Agent:           "claude",
		BillingProvider: "anthropic",
	}
}

// metricStartTimeKey holds a datapoint's StartTimeUnixNano in dimensions (#752).
// Only TimeUnixNano becomes ts, so without it an exporter retry and two distinct
// datapoints stamped the same instant are indistinguishable rows.
//
// It lives in dimensions rather than a column because every consumer reads
// dimensions by named key (dimensions->>'tool' and the like); nothing groups
// or compares the whole object, so an extra key splits no aggregate. A retry
// repeats the start time, so rows identical on (ts, dimensions) remain exactly
// the retry candidates.
const metricStartTimeKey = "start_time_unix_nano"

// withStartTime adds the datapoint's start time to dims, as a decimal string
// because nanoseconds overflow a JSON float. An unset start time adds nothing.
func withStartTime(dims map[string]interface{}, startNano uint64) map[string]interface{} {
	if startNano == 0 {
		return dims
	}
	if dims == nil {
		dims = make(map[string]interface{}, 1)
	}
	dims[metricStartTimeKey] = strconv.FormatUint(startNano, 10)
	return dims
}

// extractMetricFields pulls session_id and model out of the Dimensions map
// and into the dedicated struct fields, removing them from Dimensions to avoid
// duplication.
func extractMetricFields(om *store.OtelMetric) {
	if om.Dimensions == nil {
		return
	}
	for _, key := range []string{"session.id", "session_id"} {
		if v, ok := om.Dimensions[key].(string); ok && v != "" {
			om.SessionID = v
			delete(om.Dimensions, key)
			break
		}
	}
	if v, ok := om.Dimensions["model"].(string); ok && v != "" {
		om.Model = v
		delete(om.Dimensions, "model")
	}
}

func setNumberValue(om *store.OtelMetric, dp *metricspb.NumberDataPoint) {
	switch v := dp.Value.(type) {
	case *metricspb.NumberDataPoint_AsInt:
		om.ValueInt = &v.AsInt
	case *metricspb.NumberDataPoint_AsDouble:
		om.ValueDouble = &v.AsDouble
	}
}

func flattenNumberDPAttrs(dp *metricspb.NumberDataPoint) map[string]interface{} {
	if len(dp.Attributes) == 0 {
		return nil
	}
	return flattenAttrs(dp.Attributes)
}

func flattenHistogramDPAttrs(dp *metricspb.HistogramDataPoint) map[string]interface{} {
	if len(dp.Attributes) == 0 {
		return nil
	}
	return flattenAttrs(dp.Attributes)
}

// metricDrop describes datapoints that were received but produced no rows, so
// the caller can report the loss back to the sender.
type metricDrop struct {
	name       string
	reason     string
	dataPoints int64
}

const (
	metricSampleMaxBytes = 2048
	dropMessageMaxBytes  = 2048
	// dropNotesMax caps how many per-metric notes are collected for one export.
	dropNotesMax = 20
)

// metricDropCounts counts drops per metric name. Suppressing every repeat after
// the first line would hide unbounded data loss behind a single log entry, so
// drops are re-logged on an exponential backoff instead.
var metricDropCounts = &sync.Map{}

// metricDropKeys bounds the cardinality of metricDropCounts. The key is a
// client-supplied metric name and the HTTP OTLP port takes requests without an
// auth interceptor, so an unbounded map is remotely growable memory. Past the
// cap every further name shares one bucket: the counters stay useful for the
// names actually in use, and a flood of invented names costs a fixed amount.
const metricDropKeyCap = 1024

var (
	metricDropKeys      atomic.Int64
	lastUntrackedDropAt atomic.Int64 // unix nanos of the last untracked drop log
)

// untrackedDropLogInterval bounds how often a drop of an untracked name may be
// logged. Folding those names into one shared *counter* instead would let a
// flood push the shared count so high that the next backoff milestone is
// decades of traffic away, silencing genuine drops for good. A time gate keeps
// the volume bounded while guaranteeing real losses resurface.
var untrackedDropLogInterval = 30 * time.Second

// dropCounter returns the per-name counter, or nil once the cap is reached and
// the name is not already tracked.
//
// The check and the insert are deliberately not atomic together: making them so
// would need a mutex on a hot path to save a bounded overshoot proportional to
// the number of concurrent exports. The cap is a memory bound, not a quota.
func dropCounter(name string) *atomic.Int64 {
	if v, ok := metricDropCounts.Load(name); ok {
		return v.(*atomic.Int64)
	}
	if metricDropKeys.Load() >= metricDropKeyCap {
		return nil
	}
	v, loaded := metricDropCounts.LoadOrStore(name, new(atomic.Int64))
	if !loaded {
		metricDropKeys.Add(1)
	}
	return v.(*atomic.Int64)
}

// shouldLogDrop reports whether this drop should be logged, and how many drops
// of that name have been seen (0 when the name is not tracked).
func shouldLogDrop(name string) (int64, bool) {
	if c := dropCounter(name); c != nil {
		n := c.Add(1)
		return n, isDropLogMilestone(n)
	}
	now := time.Now().UnixNano()
	last := lastUntrackedDropAt.Load()
	if now-last < int64(untrackedDropLogInterval) {
		return 0, false
	}
	// CAS so concurrent untracked drops produce one line, not one each.
	return 0, lastUntrackedDropAt.CompareAndSwap(last, now)
}

// dropMetric logs a metric that produced no rows and returns a descriptor of
// the loss.
func dropMetric(m *metricspb.Metric, res resourceInfo, reason string) *metricDrop {
	d := &metricDrop{name: m.Name, reason: reason, dataPoints: dataPointCount(m)}

	n, ok := shouldLogDrop(d.name)
	if !ok {
		return d
	}
	count := fmt.Sprintf("drop #%d", n)
	if n == 0 {
		count = "untracked name, rate-limited"
	}
	// The sample names the datapoint attribute keys, which are otherwise
	// unrecoverable: a dropped metric leaves no row to inspect in the DB.
	log.Printf("[otel-metrics] dropped metric %q: %s (datapoints=%d, %s) service=%q session=%q user=%q sample=%s",
		d.name, d.reason, d.dataPoints, count,
		res.serviceName, res.sessionID, res.userID, metricSample(m))
	return d
}

// isDropLogMilestone reports whether the n-th drop should be logged: the 1st,
// 10th, 100th, ... so a persistent drop keeps leaving a trail without flooding.
func isDropLogMilestone(n int64) bool {
	for t := int64(1); t > 0 && t <= n; t *= 10 {
		if t == n {
			return true
		}
	}
	return false
}

// dataPointCount reports how many datapoints the metric carries. An unknown
// type reports 0 because its payload cannot be read.
func dataPointCount(m *metricspb.Metric) int64 {
	switch d := m.Data.(type) {
	case *metricspb.Metric_Sum:
		return int64(len(d.Sum.GetDataPoints()))
	case *metricspb.Metric_Gauge:
		return int64(len(d.Gauge.GetDataPoints()))
	case *metricspb.Metric_Histogram:
		return int64(len(d.Histogram.GetDataPoints()))
	case *metricspb.Metric_ExponentialHistogram:
		return int64(len(d.ExponentialHistogram.GetDataPoints()))
	case *metricspb.Metric_Summary:
		return int64(len(d.Summary.GetDataPoints()))
	}
	return 0
}

// metricSample describes a dropped metric well enough to diagnose it without
// copying user data into the log. Attribute VALUES are deliberately excluded:
// clients put account identifiers and other secrets in them, and container logs
// have looser access control and retention than the dashboard database. What a
// diagnosis needs is which attribute keys arrived, not what they held.
func metricSample(m *metricspb.Metric) string {
	keys := attrKeys(m)
	if len(keys) == 0 {
		return fmt.Sprintf("{type=%T, unit=%q, attrKeys=none}", m.Data, m.Unit)
	}
	sort.Strings(keys)
	return limit(fmt.Sprintf("{type=%T, unit=%q, attrKeys=[%s]}",
		m.Data, m.Unit, strings.Join(keys, " ")), metricSampleMaxBytes)
}

// attrKeys collects the distinct datapoint attribute keys of a metric.
func attrKeys(m *metricspb.Metric) []string {
	seen := map[string]struct{}{}
	collect := func(kvs []*commonpb.KeyValue) {
		for _, kv := range kvs {
			seen[kv.Key] = struct{}{}
		}
	}
	switch d := m.Data.(type) {
	case *metricspb.Metric_Sum:
		for _, dp := range d.Sum.GetDataPoints() {
			collect(dp.Attributes)
		}
	case *metricspb.Metric_Gauge:
		for _, dp := range d.Gauge.GetDataPoints() {
			collect(dp.Attributes)
		}
	case *metricspb.Metric_Histogram:
		for _, dp := range d.Histogram.GetDataPoints() {
			collect(dp.Attributes)
		}
	case *metricspb.Metric_ExponentialHistogram:
		for _, dp := range d.ExponentialHistogram.GetDataPoints() {
			collect(dp.Attributes)
		}
	case *metricspb.Metric_Summary:
		for _, dp := range d.Summary.GetDataPoints() {
			collect(dp.Attributes)
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	return keys
}

// limit shortens s to at most maxBytes, including the marker it appends. The
// marker itself has to fit inside the budget, or a caller that promises a
// bounded response ends up returning more than it advertised.
func limit(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	keep := maxBytes
	var marker string
	for {
		marker = fmt.Sprintf("...[+%dB]", len(s)-keep)
		if keep+len(marker) <= maxBytes {
			break
		}
		keep = maxBytes - len(marker)
		if keep <= 0 {
			// The marker alone does not fit. Prefer the content over the
			// "how much was cut" note: an empty string tells the reader nothing.
			return strings.ToValidUTF8(s[:maxBytes], "")
		}
	}
	return strings.ToValidUTF8(s[:keep], "") + marker
}
