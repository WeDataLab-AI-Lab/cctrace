package otelrecv

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"

	"github.com/jackc/pgx/v5/pgxpool"

	"cctrace/internal/buffer"
	"cctrace/internal/codexconfig"
	"cctrace/internal/emailalias"
	"cctrace/internal/queue"
	"cctrace/internal/store"
)

func metricRequest(serviceName, metricName string) *colmetricspb.ExportMetricsServiceRequest {
	return &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{mkKV("service.name", strAV(serviceName))}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: metricName,
					Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
						DataPoints: []*metricspb.NumberDataPoint{{
							TimeUnixNano: 1_700_000_000_000_000_000,
							Value:        &metricspb.NumberDataPoint_AsInt{AsInt: 1},
						}},
					}},
				}},
			}},
		}},
	}
}

// postMetrics sends req over the HTTP receiver with the given account header and
// returns what the receiver handed on for storage.
func postMetrics(t *testing.T, r *MetricsReceiver, req *colmetricspb.ExportMetricsServiceRequest, account string) []*store.OtelMetric {
	t.Helper()
	deadPool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deadPool.Close)
	r.queue = queue.New(deadPool)
	r.buffer = buffer.NewRing(16)

	srv := httptest.NewServer(NewHTTPReceiver(nil, r).Handler())
	defer srv.Close()
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/metrics", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	if account != "" {
		httpReq.Header.Set(CodexAccountHeader, account)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var got []*store.OtelMetric
	for {
		data, ok := r.buffer.Pop()
		if !ok {
			return got
		}
		var m store.OtelMetric
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		got = append(got, &m)
	}
}

// #715: the Codex exporter names its billing account in a header, since its
// metrics carry no session and no account of their own.
func TestHTTPMetricsStampCodexAccountFromHeader(t *testing.T) {
	r := NewMetricsReceiver(nil, nil, emailalias.Aliases{})

	got := postMetrics(t, r, metricRequest("codex_cli_rs", "codex.tool.call"), "acct-1")
	if len(got) != 1 || got[0].BillingProvider != "openai" || got[0].AccountID != "acct-1" {
		t.Fatalf("codex metric = %+v, want openai/acct-1", got)
	}

	// Only a Codex row is Codex billing; a Claude row on the same request is not.
	got = postMetrics(t, r, metricRequest("claude-code", "claude_code.cost.usage"), "acct-1")
	if len(got) != 1 || got[0].AccountID != "" {
		t.Fatalf("claude metric = %+v, want no account", got)
	}
}

// The same refusal #719 applies to session logs: an excluded billing account's
// metrics are not stored at all.
func TestHTTPMetricsFromAnExcludedBillingAccountAreRefused(t *testing.T) {
	r := NewMetricsReceiver(nil, nil, emailalias.Aliases{}).
		WithBillingAccountFilter(func(provider, account string) bool { return provider == "openai" && account == "acct-excluded" })

	if got := postMetrics(t, r, metricRequest("codex_cli_rs", "codex.tool.call"), "acct-excluded"); len(got) != 0 {
		t.Fatalf("kept %d metrics of an excluded billing account", len(got))
	}
	if got := postMetrics(t, r, metricRequest("codex_cli_rs", "codex.tool.call"), "acct-team"); len(got) != 1 {
		t.Fatalf("kept %d metrics of a visible account, want 1", len(got))
	}
	if got := postMetrics(t, r, metricRequest("codex_cli_rs", "codex.tool.call"), ""); len(got) != 1 {
		t.Fatalf("kept %d metrics with no account header, want 1", len(got))
	}
}

// #753: healing an extra Codex home turns on metrics that home never sent. If its
// account is excluded, those metrics must still be refused — the header the
// healed config carries has to be the one this receiver filters on.
func TestHealedExtraHomeOfAnExcludedAccountIsStillRefused(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"tokens":{"account_id":"acct-excluded"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[otel]\nendpoint = \"http://localhost:4317\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := codexconfig.HealExistingOtelBlock(home, "http://localhost:4317", "tok", true); err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(regexp.QuoteMeta(CodexAccountHeader) + ` = "([^"]+)"`).FindSubmatch(config)
	if m == nil {
		t.Fatalf("healed config names no account under %s:\n%s", CodexAccountHeader, config)
	}

	r := NewMetricsReceiver(nil, nil, emailalias.Aliases{}).
		WithBillingAccountFilter(func(provider, account string) bool { return provider == "openai" && account == "acct-excluded" })
	if got := postMetrics(t, r, metricRequest("codex_cli_rs", "codex.tool.call"), string(m[1])); len(got) != 0 {
		t.Fatalf("kept %d metrics from a healed home of an excluded account", len(got))
	}
}
