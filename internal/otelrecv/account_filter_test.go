package otelrecv

import (
	"context"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/emailalias"
)

// excludedAddress is the filter a server with one excluded address installs.
func excludedAddress(addr string) AccountFilter {
	return func(email string) bool { return strings.EqualFold(email, addr) }
}

// #715: telemetry an excluded account sends is refused at ingest, not only hidden
// on the dashboard. OTEL names the account by its login address and nothing else.
func TestLogsFromAnExcludedAddressAreRefused(t *testing.T) {
	r := (&LogsReceiver{uidEmailMap: make(map[string]string), aliases: emailalias.Aliases{}}).
		WithAccountFilter(excludedAddress("excluded@example.test"))

	excluded := claimedLogsRequest("profile@example.test", "u1")
	excluded.ResourceLogs[0].Resource.Attributes = append(
		excluded.ResourceLogs[0].Resource.Attributes, mkKV("user.email", strAV("Excluded@example.test")))
	if events := r.buildEventsWithIdentity(excluded, nil); len(events) != 0 {
		t.Errorf("kept %d events from an excluded address", len(events))
	}

	kept := claimedLogsRequest("profile@example.test", "u1")
	kept.ResourceLogs[0].Resource.Attributes = append(
		kept.ResourceLogs[0].Resource.Attributes, mkKV("user.email", strAV("colleague@example.test")))
	if events := r.buildEventsWithIdentity(kept, nil); len(events) != 1 {
		t.Errorf("kept %d events from an address nobody excluded, want 1", len(events))
	}
}

// A metric whose payload names no login address gets the token owner's address
// filled in afterwards -- a dashboard account, not the Anthropic login that sent
// it. Refusing on that filled value would drop whoever holds the token whenever
// their own dashboard address happens to be excluded. Only the address the
// payload itself sent counts.
func TestMetricsAreRefusedOnlyOnTheAddressThePayloadSent(t *testing.T) {
	r := (&MetricsReceiver{aliases: emailalias.Aliases{}}).
		WithAccountFilter(excludedAddress("excluded@example.test"))

	fromExcluded := claimedMetricsRequest("profile@example.test", "u1")
	fromExcluded.ResourceMetrics[0].Resource.Attributes = append(
		fromExcluded.ResourceMetrics[0].Resource.Attributes, mkKV("user.email", strAV("excluded@example.test")))
	if got := exportMetricsThroughGRPC(t, r, context.Background(), fromExcluded); len(got) != 0 {
		t.Errorf("kept %d metrics from an excluded address", len(got))
	}

	noAddress := claimedMetricsRequest("profile@example.test", "u1")
	r.WithIdentityResolver(func(context.Context, string) (*ClientIdentity, error) {
		return &ClientIdentity{ProfileEmail: "holder@example.test", LoginEmail: "excluded@example.test", UserID: "holder"}, nil
	})
	ctx := auth.ContextWithIngestToken(context.Background(), "tok-holder")
	if got := exportMetricsThroughGRPC(t, r, ctx, noAddress); len(got) != 1 {
		t.Errorf("kept %d metrics with no payload address, want 1 -- the token owner's address must not decide exclusion", len(got))
	}
}
