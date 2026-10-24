package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

func kv(k string, v any) *commonpb.KeyValue {
	var av *commonpb.AnyValue
	switch x := v.(type) {
	case string:
		av = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: x}}
	case int:
		av = &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(x)}}
	case int64:
		av = &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: x}}
	case float64:
		av = &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: x}}
	case bool:
		av = &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: x}}
	}
	return &commonpb.KeyValue{Key: k, Value: av}
}

func postProto(url, token, account string, msg proto.Message) error {
	body, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Authorization", "Bearer "+token)
	if account != "" {
		req.Header.Set("X-Cctrace-Codex-Account", account)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("POST %s: %d %s", url, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

// claudeEvents are the OTEL log records Claude Code emits for one persona's
// sessions: user_prompt, api_request (cost and tokens), tool_decision and
// tool_result. The dashboard's cost and token numbers come from these alone.
func claudeEvents(pl *plan, p *persona, acct accounts, homeDir string) []*logspb.LogRecord {
	var out []*logspb.LogRecord
	terminal := map[string]string{"ivy": "vscode", "priya": "iTerm.app", "yuki": "ghostty"}[p.Home]
	if terminal == "" {
		terminal = "Apple_Terminal"
	}
	rec := func(name string, at time.Time, sessionID, promptID string, attrs ...*commonpb.KeyValue) {
		base := []*commonpb.KeyValue{
			kv("event.name", name), kv("event.timestamp", iso(at)), kv("session.id", sessionID),
			kv("user.email", p.Email), kv("user.account_uuid", acct.ClaudeAccount),
			kv("organization.id", acct.ClaudeOrg), kv("terminal.type", terminal),
		}
		if promptID != "" {
			base = append(base, kv("prompt.id", promptID))
		}
		ns := uint64(at.UnixNano())
		out = append(out, &logspb.LogRecord{TimeUnixNano: ns, ObservedTimeUnixNano: ns, Attributes: append(base, attrs...)})
	}
	apiRequest := func(s *session, promptID string, req *request) {
		rec("api_request", req.End, s.ID, promptID,
			kv("model", req.Model), kv("cost_usd", req.Cost),
			kv("input_tokens", req.In), kv("output_tokens", req.Out),
			kv("cache_read_tokens", req.CacheRead), kv("cache_creation_tokens", req.CacheWrite),
			kv("duration_ms", req.End.Sub(req.Start).Milliseconds()))
	}
	for _, s := range pl.Sessions {
		if s.P != p || s.Agent != "claude" {
			continue
		}
		for _, bg := range s.Background {
			apiRequest(s, "", bg)
		}
		for _, tr := range s.Turns {
			rec("user_prompt", tr.At, s.ID, tr.PromptID, kv("prompt_length", len([]rune(tr.Prompt))))
			for _, req := range tr.Requests {
				apiRequest(s, tr.PromptID, req)
				at := req.End
				for k, a := range req.Actions {
					name := claudeToolName(a)
					source := "config"
					if req.Approval[k] != "" {
						source = req.Approval[k]
					}
					rec("tool_decision", at.Add(5*time.Millisecond), s.ID, tr.PromptID,
						kv("tool_name", name), kv("decision", "accept"), kv("source", source))
					at = at.Add(req.ToolTook[k])
					_, failed := toolOutput(tr.Task, "", a)
					rec("tool_result", at, s.ID, tr.PromptID,
						kv("tool_name", name), kv("success", !failed), kv("duration_ms", req.ToolTook[k].Milliseconds()))
				}
			}
		}
	}
	return out
}

func sendClaudeEvents(otlpBase, token string, p *persona, records []*logspb.LogRecord) error {
	res := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
		kv("service.name", "claude-code"), kv("service.version", claudeCodeVersion),
		kv("os.type", "darwin"), kv("host.arch", "arm64"),
		kv("user.id", p.UserID), kv("user.name", p.Name), kv("user.profile.email", p.Email), kv("user.team", p.Team),
	}}
	for i := 0; i < len(records); i += 2000 {
		end := min(i+2000, len(records))
		req := &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
			Resource:  res,
			ScopeLogs: []*logspb.ScopeLogs{{Scope: &commonpb.InstrumentationScope{Name: "com.anthropic.claude_code.events", Version: claudeCodeVersion}, LogRecords: records[i:end]}},
		}}}
		if err := postProto(otlpBase+"/v1/logs", token, "", req); err != nil {
			return err
		}
	}
	return nil
}

// codexMetrics are what Codex exports per turn: a delta count of tool calls by
// tool and outcome, and the turn's token usage. Codex sends no log events; its
// cost is imputed on the server from the synced rollout.
func codexMetrics(pl *plan, p *persona) []*metricspb.Metric {
	toolCalls := &metricspb.Sum{AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, IsMonotonic: true}
	usage := &metricspb.Histogram{AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA}
	for _, s := range pl.Sessions {
		if s.P != p || s.Agent != "codex" {
			continue
		}
		for _, tr := range s.Turns {
			end := tr.Requests[len(tr.Requests)-1].End
			start, stop := uint64(tr.At.UnixNano()), uint64(end.UnixNano())
			counts := map[[2]string]int64{}
			in, cached, out := 0, 0, 0
			for _, req := range tr.Requests {
				in += req.In
				cached += req.Cached
				out += req.Out
				for _, a := range req.Actions {
					_, failed := toolOutput(tr.Task, "", a)
					counts[[2]string{codexToolName(a), fmt.Sprint(!failed)}]++
				}
			}
			for k, n := range counts {
				toolCalls.DataPoints = append(toolCalls.DataPoints, &metricspb.NumberDataPoint{
					StartTimeUnixNano: start, TimeUnixNano: stop, Value: &metricspb.NumberDataPoint_AsInt{AsInt: n},
					Attributes: []*commonpb.KeyValue{kv("tool", k[0]), kv("success", k[1]), kv("model", s.Model)},
				})
			}
			for _, x := range []struct {
				kind string
				n    int
			}{{"input", in - cached}, {"cached_input", cached}, {"output", out}} {
				sum := float64(x.n)
				usage.DataPoints = append(usage.DataPoints, &metricspb.HistogramDataPoint{
					StartTimeUnixNano: start, TimeUnixNano: stop, Count: 1, Sum: &sum,
					Attributes: []*commonpb.KeyValue{kv("token.type", x.kind), kv("model", s.Model)},
				})
			}
		}
	}
	return []*metricspb.Metric{
		{Name: "codex.tool.call", Data: &metricspb.Metric_Sum{Sum: toolCalls}},
		{Name: "codex.turn.token_usage", Unit: "{token}", Data: &metricspb.Metric_Histogram{Histogram: usage}},
	}
}

func sendCodexMetrics(otlpBase, token, account string, metrics []*metricspb.Metric) error {
	res := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kv("service.name", "codex_cli_rs"), kv("service.version", codexVersion)}}
	// One export per metric keeps each request well under the receiver's limit.
	for _, m := range metrics {
		req := &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource:     res,
			ScopeMetrics: []*metricspb.ScopeMetrics{{Scope: &commonpb.InstrumentationScope{Name: "codex_otel", Version: codexVersion}, Metrics: []*metricspb.Metric{m}}},
		}}}
		if err := postProto(otlpBase+"/v1/metrics", token, account, req); err != nil {
			return err
		}
	}
	return nil
}
